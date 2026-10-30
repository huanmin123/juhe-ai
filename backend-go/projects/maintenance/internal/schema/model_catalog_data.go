// Code generated from the Node pricing catalog (seed-defaults.ts model upsert
// rows) by dump_model_catalog.mts next to this file. DO NOT hand-edit:
// regenerate with the backend tsx (see the dump script header) and re-verify
// the row count against the fresh Node dump. The rows are the 128
// static rows (39 seed parameters minus created_at/updated_at) that Node
// seedDefaults/seedPostgresDefaults insert for the built-in pricing providers
// (gpt, xai, deepseek, anthropic, gemini, glm; hybrid and openai are skipped
// exactly like Node). Row order is the Node listProviderModelPricing order
// (catalog order, release date desc, model asc) and is asserted sorted by
// model_catalog_seed_test.go. Dumped 2026-09-05. Since 2026-09-20 this file
// is maintained by manual sync from the Go pricing catalog
// (backend-go/projects/gateway/internal/pricing/data_*.go; the Node dump
// tool was retired with the archived Node backend). The 2026-09-20 sync
// basis is documented in
// docs/functions/厂商模型目录更新与清洗指南.md.
//
// 2026-10-04 M1 同步音频增补（音频视频模型接入设计 §11.2）：gpt 音频 6 行
// （gpt-4o-mini-tts / gpt-4o-transcribe / gpt-4o-mini-transcribe / tts-1 /
// tts-1-hd / whisper-1）与 gemini TTS 2 行（gemini-2.5-flash-preview-tts /
// gemini-2.5-pro-preview-tts），mode=audio、协议 audio_speech /
// audio_transcription。目录表无字符/秒价格列，tts-1 / tts-1-hd / whisper-1
// 的字符价与秒价只落在 gateway pricing 静态层
// （internal/pricing/data_openai.go，来源与数值注释见该文件）。
//
// 2026-10-04 M2 视频增补（媒体设计 §11.2）：gpt 视频 2 行（sora-2 /
// sora-2-pro），mode=video、协议 video；720p 基准秒价同样只落在 gateway
// pricing 静态层（data_openai.go，来源与分档说明见该文件）。
//
// 2026-10-04 M3 视频增补（契约 §5.2）：gemini 视频 2 行
//（veo-3.0-generate-preview / veo-3.0-fast-generate-preview），mode=video、
// 协议 video；720p 基准秒价只落在 gateway pricing 静态层
//（internal/pricing/data_gemini.go，来源与生效条件说明见该文件）。
//
// 2026-10-04 M3 视频增补（契约 §7.1）：glm 视频 1 行（cogvideox-3），
// mode=video、协议 video；不落价（CogVideoX 按次计费、无可查证秒价，
// 终态计费走契约 §2.8 兜底 0 计费 + usage_missing，依据见
// internal/pricing/data_glm.go 注释）。
//
// 2026-10-04 M5b realtime 增补（Realtime 设计 §5/契约 §4.4）：gpt realtime
// 1 行（gpt-realtime），mode=audio、协议 realtime；audio token 价
// $32/$64 per 1M 同步落在 gateway pricing 静态层与目录行
// AudioInputUsdPer1M/AudioOutputUsdPer1M（来源与不落 text 价/缓存价的
// 依据见 internal/pricing/data_openai.go 注释）。
//
// 2026-10-04 M3 回填池 xAI 增补（契约 §6.1）：xai 视频 1 行
//（grok-imagine-video-1.5），mode=video、协议 video；官方 USD 秒价未核实
//（定价子页未抓取，qwen 先例）→ 不落价，终态计费走契约 §2.8 兜底
//（duration 秒计量照抽、0 计费 + usage_missing 标记），不编造。
//
// 2026-10-05 全厂商补全批同步（自 gateway pricing data_*.go，依据
// docs/plans/计划-20261005T110000000Z-模型目录全厂商补全与场景化展示.md）：
// 141→208 行。新增 67 行：gpt +11（realtime 2.1/2/2.1-mini/1.5、
// audio-1.5、live-1、transcribe 系 4 行、gpt-5.6-cyber）、xai +2
//（grok-voice-transcribe-2.0 / grok-voice-agent）、gemini +6（veo-3.1 两行、
// 3.8-live、omni-1.1-flash、3.8 TTS 两行）、glm +10（视觉/OCR 4、tts、asr、
// image 2、video、embedding）、minimax +11（chat 3、TTS 4、asr、video 2、
// image）、volcengine +13（chat 6、video 4、image 3）、qwen +14（chat 8、
// tts 2、asr、video 2、image）。修正 34 行：gpt 图价 2 行 + ShutdownDate
// 12 行（deprecations.md 明文）+ 长上下文档 8 行（>272k：in 2x / out 1.5x）；
// xai 改价/上下文 2 行 + ShutdownDate 4 行 + grok-build-0.1 发布日置空 +
// video-1.5 发布日（上批"不落秒价/日期"口径解除）；gemini veo-3.0 两行
// ShutdownDate 2026-06-30；minimax 存量两行补 CatalogOrder 0/1。秒价/
// 字符价/按张分档与 audio cached 目录无列仍只落 gateway pricing 静态层；
// 火山/百炼/智谱人民币价按约定汇率 7.0 换算落 USD（口径与来源见各
// data_*.go 行注释）。
//
// 2026-10-05 复核批同步（自 gateway pricing data_*.go，官方页面原文回正与
// 国际站 USD 明文覆盖；教训：上一轮 xAI 部分数值引用有误，本轮以页面原文
// 回正）。209→214 行，新增 4 行（gemini gemini-3.1-flash-image /
// gemini-3-pro-image 图像、gemini-3.5-transcribe /
// gemini-3.5-transcribe-live 转写，CatalogOrder 101/102/112/113、无发布
// 日期）。修正：xai grok-4.6/4.5 价回 $2/$0.5|0.3/$6 + 上下文 500k
//（priority 档随之 4/12/1|0.6）；gemini 2.5 系三行补 CacheWriteUsdPer1M
//（0.125/0.03/0.01）、veo-3.1 两行 ShutdownDate 2026-10-22；gpt-5.2 两行
// 补 flex 档（$0.875/$7/$0.0875）；火山 3 行 chat 落国际站 USD 明文
//（turbo 0.5/2.5/0.1、lite 0.25/2/0.05+音频 3.75、mini 0.1/0.4/0.02+音频
// 1.5）；百炼 9 行价列覆盖国际站 USD 明文（chat 6 + omni 音频 3.81 +
// image 0.03 + embedding 0.07）；glm 6 行覆盖 z.ai USD 明文（4.6v/
// 4.6v-flashx/ocr/asr/image/cogview-4）。秒价/字符价/按次撤价（cogvideox
// 系）目录无对应列，只落 gateway pricing 静态层。
//
// 2026-10-05 核对批同步（自 gateway pricing data_*.go，五路只读核对代理
// 工单；行数不变 214）：gemini 回正行全部值——3.8-live（out 4.5、audio
// 3/12、image input 1）、3.8-flash-tts（0.5/9）、3.8-flash-lite-tts
//（0.5/6）、3.1-flash-image（in 0.5 + 补 text 输出 3）、3-pro-image（补
// text 输出 12）、3.5-flash tier JSON（priority storage 1.0、flex cached
// 0.075）、3.5-flash-lite tier JSON（priority storage 1.0）、veo-3.0 两行
// 日期（2025-07-31 / 2025-11-12）；布局序 gemini 段 108/109/110/111 升序
// + veo-3.0 移 embedding-2 后（新日期触发段内排序门禁重排）。gpt 回正：
// gpt-4o-2024-05-13 补 cached 2.5 + SupportsPromptCaching true；gpt-4 /
// gpt-3.5-turbo / gpt-3.5-turbo-0125 / o1 ReleaseDate 对齐计价层 raw 显式
// 日期（gpt-4 系同日行按模型名升序重排）；gpt-realtime-2.1-mini cached
// 0.06 删（text/audio cached 异值不落模态无关通道）。anthropic 15 行
// Mode 补 seedStrPtr("chat")（计价层工厂恒置 chat，系统性漏同步）。xai
// grok-imagine-image 系 3 行 Mode 统一词表 image_generation。
//
// 2026-10-05 取证批同步（自 gateway pricing data_*.go，官方页原文逐字取证
// 后落定；214→217，gemini 27→30）：omni-1.1-flash 值回正（1.5/9，audio/
// image 差价删——官方 GA/Preview 两表同价、视频生成定位，$0.5/$3+差价
// 计费偏低 3 倍作废）；新增 3 行——gemini-3.1-flash-tts-preview（107，
// legacy 在售 $1/$20）、veo-3.1-fast-generate-preview（nil order，
// $0.10/s 主档秒价只在静态层）、gemini-3.1-flash-lite-image（103，
// $0.25/$1.5/$30）；paraformer-v2 秒价只在静态层（国际站 USD 明文
// $0.000012/s 覆盖换算值），种子行无秒价列不变。
//
// 2026-10-05 遗留取证批同步（217→218，gemini 30→31）：新增
// gemini-3.5-live-translate-preview（114，Live 翻译会话面 realtime，官方
// ID 带 -preview 后缀三源逐字一致；audio $3.5/$21 落 AudioInput/Output
// 通道，text 通道官方无明文不落）；3.7-flash 模态补全只在计价层（种子无
// 模态列）。gpt-5.5-cyber 确认非公开在售维持不收录（证据修正：pricing.md
// .md 源 Cyber 表有可见行 $12.50/$1.25/-/$75，渲染页无——两源矛盾 + models
// 页 404，维持不落）；MiniMax 旧版视频 T2V/I2V/S2V 官方现行页无明文价
// 不落。openai realtime/audio 族按 pricing.md 官方表补齐（218→221，gpt
// 81→84）：新增 gpt-realtime-mini / gpt-audio / gpt-audio-mini（补全批
// 漏收、§3.3 未列）；存量 realtime 系补 Image 输入价（$5/$0.8）与
// gpt-realtime-1.5 cached $0.4（text/audio cached 同值）；旧
// gpt-realtime 行按新口径补 text $4/$16 + cached $0.4 + image $5。

package schema

import (
	"fmt"
	"sort"
)

// seedStrPtr/seedInt64Ptr/seedFloat64Ptr build the optional seed parameters
// (the Node "... ?? null" columns).
func seedStrPtr(value string) *string       { return &value }
func seedInt64Ptr(value int64) *int64       { return &value }
func seedFloat64Ptr(value float64) *float64 { return &value }

// modelCatalogSeedRow mirrors one provider_model_catalog seed row with the
// static columns of Node seedDefaults (seed-defaults.ts modelStatement) and
// seedPostgresDefaults (postgres-seed-defaults.ts modelSeedValues). JSON
// columns keep the exact Node JSON.stringify output. 自供应商与上游协议面重构
// （设计 §3）起本 struct 只是目录快照的数据源形态：seed 落库目标已切换为唯一
// 物理表 models，写入前经 modelSeedTargetRows 投影（vendor_code/public_model/
// upstream_model/scope/status/supported_endpoint_modes_json/currency 列由
// 投影规则推导，其余列按名搬运），数据行本体仍冻结在
// model_catalog_data_gen.go。
type modelCatalogSeedRow struct {
	ID                                      string
	ProviderCode                            string
	Model                                   string
	Mode                                    *string
	CatalogOrder                            *int64
	ReleaseDate                             *string
	ShutdownDate                            *string
	SupportedAPIProtocolsJSON               string
	SupportedServiceTiersJSON               string
	SupportedReasoningEffortsJSON           string
	DefaultReasoningEffort                  *string
	CodexSupportedReasoningLevelsJSON       string
	CodexDefaultReasoningLevel              *string
	CodexMultiAgentVersion                  *string
	ContextWindowTokens                     *int64
	MaxInputTokens                          *int64
	MaxOutputTokens                         *int64
	MaxTokens                               *int64
	InputUsdPer1M                           *float64
	OutputUsdPer1M                          *float64
	CachedInputUsdPer1M                     *float64
	CacheWriteUsdPer1M                      *float64
	CacheWrite1HUsdPer1M                    *float64
	CacheStorageUsdPer1MPerHour             *float64
	ServiceTierPricesJSON                   string
	LongContextInputTokenThreshold          *int64
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64
	ImageInputUsdPer1M                      *float64
	ImageOutputUsdPer1M                     *float64
	AudioInputUsdPer1M                      *float64
	AudioOutputUsdPer1M                     *float64
	OutputUsdPerImage                       *float64
	SupportsPromptCaching                   bool
	CatalogVisible                          bool
	Source                                  string
}

// modelCatalogSeedRows 已迁移至 model_catalog_data_gen.go（Code generated，
// 由 gateway pricing.TestGenerateSeedCatalog 从 catalogdata/*.json 生成，
// 计划-20261005T230500000Z 步骤 4）——手工同步废除。seed 行的 JSON→39 列
// 投影规则见生成器 seedRowLiteral；种子 helper（seedStrPtr 等）留在本文件。
// 历史手写批次记录见 git 历史与 docs/functions/厂商模型目录更新与清洗指南.md。

// modelSeedTargetRow 是一条目录快照行向 models 表（供应商与上游协议面重构
// 设计 §3）的目标投影：identity 列（vendor_code/public_model/upstream_model/
// scope/status）与 supported_endpoint_modes_json 由投影规则推导，其余列按名
// 搬运。scope/system_account_id/currency 是语句内字面量，不入 struct。
type modelSeedTargetRow struct {
	ID                                      string
	VendorCode                              string
	PublicModel                             string
	UpstreamModel                           string
	Status                                  string
	CatalogVisible                          bool
	CatalogOrder                            *int64
	ReleaseDate                             *string
	ShutdownDate                            *string
	SupportedEndpointModesJSON              string
	SupportedServiceTiersJSON               string
	SupportedReasoningEffortsJSON           string
	DefaultReasoningEffort                  *string
	CodexSupportedReasoningLevelsJSON       string
	CodexDefaultReasoningLevel              *string
	CodexMultiAgentVersion                  *string
	ContextWindowTokens                     *int64
	MaxInputTokens                          *int64
	MaxOutputTokens                         *int64
	MaxTokens                               *int64
	SupportsPromptCaching                   bool
	Source                                  string
	InputUsdPer1M                           *float64
	OutputUsdPer1M                          *float64
	CachedInputUsdPer1M                     *float64
	CacheWriteUsdPer1M                      *float64
	CacheWrite1HUsdPer1M                    *float64
	CacheStorageUsdPer1MPerHour             *float64
	ServiceTierPricesJSON                   string
	LongContextInputTokenThreshold          *int64
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64
	ImageInputUsdPer1M                      *float64
	ImageOutputUsdPer1M                     *float64
	AudioInputUsdPer1M                      *float64
	AudioOutputUsdPer1M                     *float64
	OutputUsdPerImage                       *float64
}

// modelSeedStatusActive / modelSeedStatusDisabled 是 models.status 在 seed
// 路径上会写出的两个枚举值（设计 §3：draft 只能来自应用层，seed 不写）。
const (
	modelSeedStatusActive   = "active"
	modelSeedStatusDisabled = "disabled"
)

// modelSeedBuiltinScope / modelSeedBuiltinCurrency 是 builtin seed 行的固定
// scope 与 currency（任务契约：builtin 统一 'USD'；scope='builtin' 且
// system_account_id=NULL 由 models 表 CHECK 联动校验）。
const (
	modelSeedBuiltinScope    = "builtin"
	modelSeedBuiltinCurrency = "USD"
)

// modelSeedProtocolEndpointModes 把目录行声明的上游 API 协议名映射到设计
// §2.2 的 25 值 endpoint mode 词表；map 缺键视为未接入的协议并让 seed 报错
// （deny-by-default，禁止对未知协议静默产空 mode 集）。"completions" 是旧
// /v1/completions 遗留声明，目标协议面无对应 mode，显式映射为空集。
var modelSeedProtocolEndpointModes = map[string][]string{
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

// modelSeedEndpointModesJSON 从目录行的 SupportedAPIProtocolsJSON 推导
// models.supported_endpoint_modes_json（设计 §3：模型真实支持的精确 mode
// 集合，元素唯一且按 ASCII 升序；active 行至少 1 个 mode）。
func modelSeedEndpointModesJSON(row modelCatalogSeedRow) (string, error) {
	protocols, err := parseSeedStringArray(row.SupportedAPIProtocolsJSON)
	if err != nil {
		return "", fmt.Errorf("model seed row %s: parse supported_api_protocols_json: %w", row.ID, err)
	}
	seen := map[string]bool{}
	modes := make([]string, 0, 8)
	for _, protocol := range protocols {
		declared, known := modelSeedProtocolEndpointModes[protocol]
		if !known {
			return "", fmt.Errorf("model seed row %s: unsupported supported API protocol %q", row.ID, protocol)
		}
		for _, mode := range declared {
			if !seen[mode] {
				seen[mode] = true
				modes = append(modes, mode)
			}
		}
	}
	if len(modes) == 0 {
		return "", fmt.Errorf("model seed row %s: derived empty supported_endpoint_modes_json", row.ID)
	}
	sort.Strings(modes)
	return seedStringify(modes), nil
}

// modelSeedRowStatus 沿用内置过期模型判定（原迁移窗口的 shutdown 过滤方向）：
// shutdown_date 已过（<= 当前 UTC 日期）写 'disabled'，否则 'active'。
// asOfUTCDate 形态为 YYYY-MM-DD。
func modelSeedRowStatus(row modelCatalogSeedRow, asOfUTCDate string) string {
	if row.ShutdownDate != nil && *row.ShutdownDate <= asOfUTCDate {
		return modelSeedStatusDisabled
	}
	return modelSeedStatusActive
}

// modelSeedTargetRows 把全部目录快照行投影为 models 表目标行；目录数据行数
// 不变（含已过期 disabled 行，设计 §3 允许 disabled 行 mode 为空——但推导
// 规则保证每行至少 1 个 mode）。
func modelSeedTargetRows(asOfUTCDate string) ([]modelSeedTargetRow, error) {
	rows := make([]modelSeedTargetRow, 0, len(modelCatalogSeedRows))
	for _, source := range modelCatalogSeedRows {
		modesJSON, err := modelSeedEndpointModesJSON(source)
		if err != nil {
			return nil, err
		}
		rows = append(rows, modelSeedTargetRow{
			ID:                                      source.ID,
			VendorCode:                              source.ProviderCode,
			PublicModel:                             source.Model,
			UpstreamModel:                           source.Model,
			Status:                                  modelSeedRowStatus(source, asOfUTCDate),
			CatalogVisible:                          source.CatalogVisible,
			CatalogOrder:                            source.CatalogOrder,
			ReleaseDate:                             source.ReleaseDate,
			ShutdownDate:                            source.ShutdownDate,
			SupportedEndpointModesJSON:              modesJSON,
			SupportedServiceTiersJSON:               source.SupportedServiceTiersJSON,
			SupportedReasoningEffortsJSON:           source.SupportedReasoningEffortsJSON,
			DefaultReasoningEffort:                  source.DefaultReasoningEffort,
			CodexSupportedReasoningLevelsJSON:       source.CodexSupportedReasoningLevelsJSON,
			CodexDefaultReasoningLevel:              source.CodexDefaultReasoningLevel,
			CodexMultiAgentVersion:                  source.CodexMultiAgentVersion,
			ContextWindowTokens:                     source.ContextWindowTokens,
			MaxInputTokens:                          source.MaxInputTokens,
			MaxOutputTokens:                         source.MaxOutputTokens,
			MaxTokens:                               source.MaxTokens,
			SupportsPromptCaching:                   source.SupportsPromptCaching,
			Source:                                  source.Source,
			InputUsdPer1M:                           source.InputUsdPer1M,
			OutputUsdPer1M:                          source.OutputUsdPer1M,
			CachedInputUsdPer1M:                     source.CachedInputUsdPer1M,
			CacheWriteUsdPer1M:                      source.CacheWriteUsdPer1M,
			CacheWrite1HUsdPer1M:                    source.CacheWrite1HUsdPer1M,
			CacheStorageUsdPer1MPerHour:             source.CacheStorageUsdPer1MPerHour,
			ServiceTierPricesJSON:                   source.ServiceTierPricesJSON,
			LongContextInputTokenThreshold:          source.LongContextInputTokenThreshold,
			LongContextInputTokenThresholdInclusive: source.LongContextInputTokenThresholdInclusive,
			LongContextInputCostMultiplier:          source.LongContextInputCostMultiplier,
			LongContextOutputCostMultiplier:         source.LongContextOutputCostMultiplier,
			ImageInputUsdPer1M:                      source.ImageInputUsdPer1M,
			ImageOutputUsdPer1M:                     source.ImageOutputUsdPer1M,
			AudioInputUsdPer1M:                      source.AudioInputUsdPer1M,
			AudioOutputUsdPer1M:                     source.AudioOutputUsdPer1M,
			OutputUsdPerImage:                       source.OutputUsdPerImage,
		})
	}
	return rows, nil
}
