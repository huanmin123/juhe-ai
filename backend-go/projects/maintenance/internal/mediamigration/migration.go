// Package mediamigration 实现「非对话媒体统一计费」P3 的 manual-override
// 存量行一次性换算（docs/functions/非对话媒体统一计费设计.md §9.2/§9.3）。
//
// 背景：models.media_pricing_json 为空时运行时继续 legacy 计价；seed 对
// source='manual-override' 且任一 legacy 媒体价格列非空的行保持 NULL，
// 防止目录价静默覆盖人工价。上线统一模板前必须把这些行的现有 legacy 值
// 按 §9.3 同一规则换算成 mediaPricing，否则它们永远停留在旧口径。
//
// 可换算范围严格限定在 models 表真实存在的 legacy 媒体列：
//   - output_usd_per_image → image 张价；
//   - image_output_usd_per_1m → image token 价 × 每张 token 估算；
//   - audio_output_usd_per_1m → TTS 每秒价 × 每秒 token 估算；
//   - audio_input_usd_per_1m → STT 每秒价 × 每秒 token 估算。
//
// 视频秒价/次价、TTS 字符价/秒价、STT 秒价不在 models 表（只存在于网关
// 静态定价层），因此不在此脚本的换算范围内；manual-override 行的这些
// 维度没有 DB 人工价可迁移。realtime 与 chat 音频行按设计保持 legacy。
//
// 无法确定的行一律不写，保持 media_pricing_json=NULL 并在 manifest 的
// manual_review 中列出，由维护者人工补价；绝不猜测档位或价格。
package mediamigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// ErrMediaPricingColumnMissing 表示目标库尚未执行 revision 26 加法相；
// 迁移必须在 upgrade --phase=expand（或 --ensure-schema）之后执行。
var ErrMediaPricingColumnMissing = errors.New("业务库缺少 juhe_business.models 或 media_pricing_json；请核对 --dsn 并先执行 upgrade --phase=expand（或 --ensure-schema）")

// ErrApplyRequiresWrites 防止调用方把 dry-run 选项传给 Apply 造成误写。
var ErrApplyRequiresWrites = errors.New("Apply 不接受 DryRun；只读盘点请使用 BuildPlan")

// 行动作。
const (
	ActionConvert      = "convert"
	ActionManualReview = "manual_review"
	ActionSkip         = "skip"
)

// 媒体类型。
const (
	KindImage    = "image"
	KindTTS      = "tts"
	KindSTT      = "stt"
	KindVideo    = "video"
	KindRealtime = "realtime"
	KindNonMedia = "non_media"
	KindUnknown  = "unknown"
)

// 换算常量（设计 §9.3）：与 P3 catalogdata 迁移使用同一组估算值。
const (
	// AudioTokensPerSecond 是 TTS/STT 每秒音频的 token 估算数；Gemini 与
	// OpenAI 都按 25 保守估算。
	AudioTokensPerSecond = 25.0
	// GeminiImageTokensPerImage 是 Gemini 图片每张 token 估算数。
	GeminiImageTokensPerImage = 1120.0
	// OpenAIImageTokensPerImage 是 OpenAI 1024×1024 medium 图片每张 token
	// 估算数。
	OpenAIImageTokensPerImage = 1056.0
)

// Options 是 BuildPlan/Apply 的注入参数。
type Options struct {
	// DryRun 只影响 manifest 的 dryRun 字段；BuildPlan 本身全程只读。
	DryRun bool
	// CatalogMediaPricing 返回内置目录行（provider/model）的 mediaPricing
	// JSON。STT 的 legacy 权威价格是静态秒价，models 表只有 audio input
	// token 价；只有 token×25 与目录秒价一致时才允许自动换算，否则人工
	// 复核。nil 表示无目录查询能力，所有 STT token 行进入 manual_review。
	CatalogMediaPricing func(providerCode, model string) (string, bool)
}

// Legacy 是一行 models 的 legacy 媒体价格快照（nil = NULL）。
type Legacy struct {
	ImageInputUsdPer1M  *float64 `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUsdPer1M *float64 `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUsdPer1M  *float64 `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUsdPer1M *float64 `json:"audioOutputUsdPer1M,omitempty"`
	OutputUsdPerImage   *float64 `json:"outputUsdPerImage,omitempty"`
}

// Row 是单个 manual-override 行的盘点/换算结果。
type Row struct {
	ID               string `json:"id"`
	ProviderCode     string `json:"providerCode"`
	Model            string `json:"model"`
	Scope            string `json:"scope"`
	Kind             string `json:"kind"`
	Action           string `json:"action"`
	Reason           string `json:"reason,omitempty"`
	Formula          string `json:"formula,omitempty"`
	Legacy           Legacy `json:"legacy"`
	MediaPricingJSON string `json:"mediaPricingJson,omitempty"`
}

// Plan 是 dry-run manifest 与 apply 报告共用的结构。
type Plan struct {
	Database     string `json:"database"`
	Mode         string `json:"mode"`
	DryRun       bool   `json:"dryRun"`
	Scanned      int    `json:"scanned"`
	Convertible  int    `json:"convertible"`
	Applied      int    `json:"applied"`
	ManualReview int    `json:"manualReview"`
	Skipped      int    `json:"skipped"`
	Rows         []Row  `json:"rows"`
}

// queryer 让 BuildPlan 同时接受 *sql.DB（dry-run）与 *sql.Tx（apply 内重盘）。
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type sourceRow struct {
	ID                   string
	ProviderCode         string
	Model                string
	Scope                string
	EndpointModesJSON    sql.NullString
	OutputModalitiesJSON sql.NullString
	ImageInputUsdPer1M   sql.NullFloat64
	ImageOutputUsdPer1M  sql.NullFloat64
	AudioInputUsdPer1M   sql.NullFloat64
	AudioOutputUsdPer1M  sql.NullFloat64
	OutputUsdPerImage    sql.NullFloat64
}

const candidateQuery = `
SELECT id, COALESCE(vendor_code, ''), public_model, scope,
       supported_endpoint_modes_json, output_modalities_json,
       image_input_usd_per_1m, image_output_usd_per_1m,
       audio_input_usd_per_1m, audio_output_usd_per_1m, output_usd_per_image
FROM juhe_business.models
WHERE source = 'manual-override'
  AND (media_pricing_json IS NULL OR btrim(media_pricing_json) = '')
  AND (image_input_usd_per_1m IS NOT NULL
       OR image_output_usd_per_1m IS NOT NULL
       OR audio_input_usd_per_1m IS NOT NULL
       OR audio_output_usd_per_1m IS NOT NULL
       OR output_usd_per_image IS NOT NULL)
ORDER BY COALESCE(vendor_code, ''), public_model, id`

const updateSQL = `
UPDATE juhe_business.models
SET media_pricing_json = $1
WHERE id = $2
  AND source = 'manual-override'
  AND (media_pricing_json IS NULL OR btrim(media_pricing_json) = '')`

// BuildPlan 只读盘点全部候选行并给出换算计划。
func BuildPlan(ctx context.Context, db *sql.DB, options Options) (Plan, error) {
	return buildPlan(ctx, db, options)
}

// Apply 在单个业务库事务内重新盘点并写入可换算行。无法换算的行保持
// NULL；任一写入失败整体回滚。重复执行幂等（已写入行不再进入候选集）。
func Apply(ctx context.Context, db *sql.DB, options Options) (Plan, error) {
	if options.DryRun {
		return Plan{}, ErrApplyRequiresWrites
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, fmt.Errorf("开启媒体价格迁移事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// 停写失效时快速失败，避免维护窗口长时间阻塞。
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
		return Plan{}, fmt.Errorf("设置事务锁超时: %w", err)
	}
	plan, err := buildPlan(ctx, tx, options)
	if err != nil {
		return Plan{}, err
	}
	plan.Mode = "apply"
	plan.DryRun = false
	for index := range plan.Rows {
		row := &plan.Rows[index]
		if row.Action != ActionConvert {
			continue
		}
		result, execErr := tx.ExecContext(ctx, updateSQL, row.MediaPricingJSON, row.ID)
		if execErr != nil {
			return Plan{}, fmt.Errorf("写入模型 %s 的 media_pricing_json: %w", row.ID, execErr)
		}
		affected, affectedErr := result.RowsAffected()
		if affectedErr != nil {
			return Plan{}, fmt.Errorf("读取模型 %s 的写入行数: %w", row.ID, affectedErr)
		}
		if affected != 1 {
			return Plan{}, fmt.Errorf("模型 %s 在迁移期间被修改（命中 %d 行），已回滚", row.ID, affected)
		}
		plan.Applied++
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, fmt.Errorf("提交媒体价格迁移事务: %w", err)
	}
	return plan, nil
}

func buildPlan(ctx context.Context, db queryer, options Options) (Plan, error) {
	plan := Plan{Mode: "plan", DryRun: options.DryRun, Rows: []Row{}}
	var columnPresent bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'juhe_business' AND table_name = 'models' AND column_name = 'media_pricing_json'
	)`).Scan(&columnPresent); err != nil {
		return Plan{}, fmt.Errorf("检查 media_pricing_json 列: %w", err)
	}
	if !columnPresent {
		return Plan{}, ErrMediaPricingColumnMissing
	}
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&plan.Database); err != nil {
		return Plan{}, fmt.Errorf("读取当前数据库名: %w", err)
	}
	rows, err := db.QueryContext(ctx, candidateQuery)
	if err != nil {
		return Plan{}, fmt.Errorf("盘点 manual-override 媒体价格行: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source sourceRow
		if err := rows.Scan(
			&source.ID, &source.ProviderCode, &source.Model, &source.Scope,
			&source.EndpointModesJSON, &source.OutputModalitiesJSON,
			&source.ImageInputUsdPer1M, &source.ImageOutputUsdPer1M,
			&source.AudioInputUsdPer1M, &source.AudioOutputUsdPer1M,
			&source.OutputUsdPerImage,
		); err != nil {
			return Plan{}, fmt.Errorf("扫描 manual-override 媒体价格行: %w", err)
		}
		row := decideWithCatalog(source, options.CatalogMediaPricing)
		switch row.Action {
		case ActionConvert:
			plan.Convertible++
		case ActionManualReview:
			plan.ManualReview++
		default:
			plan.Skipped++
		}
		plan.Rows = append(plan.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Plan{}, fmt.Errorf("遍历 manual-override 媒体价格行: %w", err)
	}
	plan.Scanned = len(plan.Rows)
	return plan, nil
}

func decide(source sourceRow) Row {
	return decideWithCatalog(source, nil)
}

func decideWithCatalog(source sourceRow, catalogMediaPricing func(providerCode, model string) (string, bool)) Row {
	row := Row{
		ID:           source.ID,
		ProviderCode: source.ProviderCode,
		Model:        source.Model,
		Scope:        source.Scope,
		Legacy: Legacy{
			ImageInputUsdPer1M:  nullableFloat(source.ImageInputUsdPer1M),
			ImageOutputUsdPer1M: nullableFloat(source.ImageOutputUsdPer1M),
			AudioInputUsdPer1M:  nullableFloat(source.AudioInputUsdPer1M),
			AudioOutputUsdPer1M: nullableFloat(source.AudioOutputUsdPer1M),
			OutputUsdPerImage:   nullableFloat(source.OutputUsdPerImage),
		},
	}
	kind, reason := classify(source)
	row.Kind = kind
	if reason != "" && kind != KindImage && kind != KindTTS && kind != KindSTT {
		row.Action = ActionSkip
		// image_input_only 只有输入图价、没有输出张价，无法判定是否为图像
		// 生成行（也可能是视觉对话行），不猜测输出价：留 manual_review 由
		// 维护者裁决（设计 §9.3 判定表）。
		if kind == KindUnknown || reason == "image_input_only" {
			row.Action = ActionManualReview
		}
		row.Reason = reason
		return row
	}
	switch kind {
	case KindImage:
		return convertImage(row, source)
	case KindTTS:
		return convertAudio(row, source, true, catalogMediaPricing)
	case KindSTT:
		return convertAudio(row, source, false, catalogMediaPricing)
	default:
		row.Action = ActionSkip
		row.Reason = reason
		return row
	}
}

// classify 从 endpoint modes 与输出模态判定媒体族。优先级：
// realtime/video 保持 legacy；audio_speech=TTS；audio_transcription=STT；
// images_json=image；generate_content 图片行用 output_modalities 含 image
// 且存在图片输出价格兜底。families 冲突时进入 manual review。
func classify(source sourceRow) (kind string, reason string) {
	modes, modesErr := parseJSONStringArray(source.EndpointModesJSON)
	if modesErr != nil {
		return KindUnknown, "invalid_endpoint_modes"
	}
	outputModalities, outputErr := parseJSONStringArray(source.OutputModalitiesJSON)
	if outputErr != nil {
		outputModalities = nil
	}
	hasRealtime := containsString(modes, "realtime_session")
	hasVideo := containsPrefix(modes, "video_")
	hasTTS := containsString(modes, "audio_speech")
	hasSTT := containsString(modes, "audio_transcription_json") || containsString(modes, "audio_job_create")
	hasImagesMode := containsString(modes, "images_json")
	hasImagePrice := source.OutputUsdPerImage.Valid || source.ImageOutputUsdPer1M.Valid
	hasImageOutput := containsString(outputModalities, "image")

	switch {
	case hasRealtime:
		return KindRealtime, "realtime_stays_legacy"
	case hasVideo:
		return KindVideo, "video_has_no_db_legacy_price"
	case hasTTS && hasSTT, hasTTS && hasImagesMode, hasSTT && hasImagesMode:
		return KindUnknown, "ambiguous_media_modes"
	case hasTTS:
		return KindTTS, ""
	case hasSTT:
		return KindSTT, ""
	case hasImagesMode:
		return KindImage, ""
	case hasImagePrice && (hasImageOutput || len(modes) == 0):
		return KindImage, ""
	case hasImagePrice:
		return KindUnknown, "image_output_price_without_image_output"
	case (source.AudioInputUsdPer1M.Valid || source.AudioOutputUsdPer1M.Valid) && hasChatMode(modes):
		// gpt-audio 等 chat/realtime 之外的对话音频行继续按 token 计费，
		// 不迁移到秒模板（设计 §9.3 的 3 行 gpt-audio 保持现状）。
		return KindNonMedia, "chat_audio_stays_legacy"
	case source.AudioInputUsdPer1M.Valid || source.AudioOutputUsdPer1M.Valid:
		return KindUnknown, "media_kind_undetermined"
	case source.ImageInputUsdPer1M.Valid:
		return KindNonMedia, "image_input_only"
	default:
		return KindNonMedia, "non_media_row"
	}
}

func convertImage(row Row, source sourceRow) Row {
	if source.OutputUsdPerImage.Valid {
		return finishConvert(row, mediapricing.KindImage, source.OutputUsdPerImage.Float64, "output_usd_per_image")
	}
	if !source.ImageOutputUsdPer1M.Valid {
		row.Action = ActionManualReview
		row.Reason = "image_output_price_missing"
		return row
	}
	tokensPerImage, ok := imageTokensPerImage(source.ProviderCode)
	if !ok {
		row.Action = ActionManualReview
		row.Reason = "unknown_image_token_constant"
		return row
	}
	baseUSD := source.ImageOutputUsdPer1M.Float64 / 1_000_000 * tokensPerImage
	formula := fmt.Sprintf("image_output_usd_per_1m / 1000000 * %.0f", tokensPerImage)
	return finishConvert(row, mediapricing.KindImage, baseUSD, formula)
}

func convertAudio(row Row, source sourceRow, tts bool, catalogMediaPricing func(providerCode, model string) (string, bool)) Row {
	var price sql.NullFloat64
	var sourceName string
	if tts {
		price = source.AudioOutputUsdPer1M
		sourceName = "audio_output_usd_per_1m"
	} else {
		price = source.AudioInputUsdPer1M
		sourceName = "audio_input_usd_per_1m"
	}
	if !price.Valid {
		row.Action = ActionManualReview
		if tts {
			row.Reason = "tts_output_price_missing"
		} else {
			row.Reason = "stt_input_price_missing"
		}
		return row
	}
	if !audioTokenProvider(source.ProviderCode) {
		row.Action = ActionManualReview
		row.Reason = "unknown_audio_token_constant"
		return row
	}
	baseUSD := price.Float64 / 1_000_000 * AudioTokensPerSecond
	formula := fmt.Sprintf("%s / 1000000 * %.0f", sourceName, AudioTokensPerSecond)
	if !tts {
		// STT 的 legacy 权威价格是静态秒价；models 表只有 input audio token
		// 价。token×25 只有在与目录秒价一致时才能安全自动换算（例如
		// gemini-3.5-transcribe 5e-05）；gpt-4o-transcribe 的 token×25
		// (1.5e-04) 与目录秒价 (1e-04) 不一致，必须人工复核，避免静默改价。
		catalogBaseUSD, ok := catalogAudioSecondsBaseUSD(catalogMediaPricing, source)
		if !ok || math.Abs(catalogBaseUSD-baseUSD) > 1e-9 {
			row.Action = ActionManualReview
			row.Reason = "stt_seconds_price_ambiguous"
			return row
		}
	}
	return finishConvert(row, mediapricing.KindAudio, baseUSD, formula)
}

// catalogAudioSecondsBaseUSD 读取内置目录行的 audio second mediaPricing；
// 仅接受无档位的 unit=second（音频模板）。
func catalogAudioSecondsBaseUSD(catalogMediaPricing func(providerCode, model string) (string, bool), source sourceRow) (float64, bool) {
	if catalogMediaPricing == nil {
		return 0, false
	}
	raw, ok := catalogMediaPricing(source.ProviderCode, source.Model)
	if !ok || strings.TrimSpace(raw) == "" {
		return 0, false
	}
	pricing, err := mediapricing.Parse([]byte(raw))
	if err != nil || pricing.Unit != mediapricing.UnitSecond || len(pricing.TierMultipliers) != 0 {
		return 0, false
	}
	return pricing.BaseUsd, true
}

// finishConvert 构造并校验统一模板；非法价格（负数/NaN/超上限等）不写，
// 转为 manual_review。
func finishConvert(row Row, kind mediapricing.Kind, baseUSD float64, formula string) Row {
	if math.IsNaN(baseUSD) || math.IsInf(baseUSD, 0) || baseUSD < 0 {
		row.Action = ActionManualReview
		row.Reason = "invalid_legacy_price"
		return row
	}
	pricing := &mediapricing.Pricing{
		Version:         mediapricing.Version,
		BaseUsd:         baseUSD,
		TierMultipliers: map[string]float64{},
	}
	switch kind {
	case mediapricing.KindImage:
		multipliers, err := mediapricing.DefaultTierMultipliers(mediapricing.UnitImage, mediapricing.ImageTier1K)
		if err != nil {
			row.Action = ActionManualReview
			row.Reason = "invalid_converted_price"
			return row
		}
		pricing.Unit = mediapricing.UnitImage
		pricing.BaseTier = mediapricing.ImageTier1K
		pricing.TierMultipliers = multipliers
	case mediapricing.KindAudio:
		pricing.Unit = mediapricing.UnitSecond
	default:
		row.Action = ActionManualReview
		row.Reason = "unsupported_media_kind"
		return row
	}
	if err := pricing.ValidateFor(kind); err != nil {
		row.Action = ActionManualReview
		row.Reason = "invalid_converted_price"
		return row
	}
	canonical, err := pricing.CanonicalString()
	if err != nil {
		row.Action = ActionManualReview
		row.Reason = "invalid_converted_price"
		return row
	}
	row.Action = ActionConvert
	row.Reason = ""
	row.Formula = formula
	row.MediaPricingJSON = canonical
	return row
}

// audioTokenProvider 限定 token→秒换算的模型族：设计 §9.3 只给 Gemini 与
// OpenAI（gpt）声明 25 tokens/s 估算；其他厂商没有同一常量，进入人工复核。
func audioTokenProvider(providerCode string) bool {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "gemini", "gpt", "openai":
		return true
	default:
		return false
	}
}

func imageTokensPerImage(providerCode string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "gemini":
		return GeminiImageTokensPerImage, true
	case "gpt", "openai":
		return OpenAIImageTokensPerImage, true
	default:
		return 0, false
	}
}

func parseJSONStringArray(value sql.NullString) ([]string, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(value.String), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// hasChatMode 识别对话/响应/生成内容类 endpoint mode；用于把 gpt-audio
// 这类“chat 协议 + audio token 价”的行留在 legacy，而不是误判为 TTS/STT。
func hasChatMode(modes []string) bool {
	for _, mode := range modes {
		switch mode {
		case "chat_json", "chat_sse", "responses_json", "responses_sse",
			"messages_json", "messages_sse", "generate_content_json",
			"generate_content_sse", "interactions_json", "interactions_sse",
			"embed_content", "count_tokens", "message_token_counting":
			return true
		}
	}
	return false
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid || math.IsNaN(value.Float64) || math.IsInf(value.Float64, 0) {
		// NaN/Inf 无法进入 JSON manifest；原值是否合法由 finishConvert 的
		// invalid_legacy_price 分支负责，这里只保证 manifest 可序列化。
		return nil
	}
	copied := value.Float64
	return &copied
}
