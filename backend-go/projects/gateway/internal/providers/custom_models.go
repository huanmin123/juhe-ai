// custom_models.go owns the custom model write domain over the models 单表
// （供应商与上游协议面重构设计 §3；scope='global'（system_account_id NULL）与
// 'personal'（=owner）行承自定义模型）："自定义模型"= custom_provider_models
// 的直迁，写路径落 public_model/upstream_model（同值写入，Builtin 行默认
// public=upstream 的同款默认）/vendor_code/status/supported_endpoint_modes_json。
// 旧 `mode`（text/image/audio/video 四值 UI 分类）与
// supported_api_protocols_json 无物理列：读取按 UI 分类逆映射派生（见
// deriveCustomUIMode / DeriveLegacyModeAndProtocols），写入经
// ForwardCustomModelEndpointModes 合成 modes json。SQLite and PostgreSQL share
// the statement text; the store binds ? -> $N for PostgreSQL.
package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/requestoverride"
)

// customProviderModelRecord mirrors CustomProviderModelRecord.
type customProviderModelRecord struct {
	ID                          string
	ProviderCode                string
	Model                       string
	Scope                       string
	SystemAccountID             *string
	Status                      string
	CatalogVisible              bool
	Mode                        *string
	SupportedAPIProtocols       []string
	SupportedServiceTiers       []string
	SupportedReasoningEfforts   []string
	SupportedToolsByProtocol    map[string][]string
	InputModalities             []string
	OutputModalities            []string
	DefaultReasoningEffort      *string
	ReleaseDate                 *string
	ShutdownDate                *string
	ContextWindowTokens         *int64
	MaxInputTokens              *int64
	MaxOutputTokens             *int64
	InputUsdPer1M               *float64
	OutputUsdPer1M              *float64
	CachedInputUsdPer1M         *float64
	CacheWriteUsdPer1M          *float64
	CacheWrite1hUsdPer1M        *float64
	CacheStorageUsdPer1MPerHour *float64
	ServiceTierPrices           map[string]ModelPriceSet
	ImageInputUsdPer1M          *float64
	ImageOutputUsdPer1M         *float64
	AudioInputUsdPer1M          *float64
	AudioOutputUsdPer1M         *float64
	OutputUsdPerImage           *float64
	PricingNotes                *string
	CapabilityNotes             *string
	Notes                       *string
	CreatedBy                   string
	UpdatedBy                   *string
	CreatedAt                   string
	UpdatedAt                   string
}

// customProviderModelUpsertInput mirrors UpsertCustomProviderModelInput with
// the route-normalized shape: every field carries the effective value (the
// Node route spreads the merged record over the patch), and nil maps to the
// repository's NULL write.
type customProviderModelUpsertInput struct {
	ID                          string
	ProviderCode                string
	Model                       string
	Scope                       string
	SystemAccountID             string
	Status                      string
	Mode                        *string
	SupportedAPIProtocols       []string
	SupportedServiceTiers       []string
	SupportedReasoningEfforts   []string
	SupportedToolsByProtocol    map[string][]string
	InputModalities             []string
	OutputModalities            []string
	DefaultReasoningEffort      *string
	ReleaseDate                 *string
	ShutdownDate                *string
	ContextWindowTokens         *int64
	MaxInputTokens              *int64
	MaxOutputTokens             *int64
	InputUsdPer1M               *float64
	OutputUsdPer1M              *float64
	CachedInputUsdPer1M         *float64
	CacheWriteUsdPer1M          *float64
	CacheWrite1hUsdPer1M        *float64
	CacheStorageUsdPer1MPerHour *float64
	ServiceTierPrices           map[string]ModelPriceSet
	ImageInputUsdPer1M          *float64
	ImageOutputUsdPer1M         *float64
	AudioInputUsdPer1M          *float64
	AudioOutputUsdPer1M         *float64
	OutputUsdPerImage           *float64
	PricingNotes                *string
	CapabilityNotes             *string
	Notes                       *string
	ActorSystemAccountID        string
}

// customProviderModelMutationRecord mirrors CustomProviderModelMutationRecord.
type customProviderModelMutationRecord struct {
	ID              string
	ProviderCode    string
	Model           string
	Scope           string
	SystemAccountID *string
	Status          string
	CatalogVisible  bool
	ShutdownDate    *string
	UpdatedAt       string
}

// customModelPatchOutcome mirrors CustomProviderModelPatchOutcome.
type customModelPatchOutcome struct {
	Kind                                   string // updated | no_op | conflict
	Record                                 customProviderModelMutationRecord
	ClearedDefaultHealthCheckProviderCodes []string
}

// customProviderModelColumns 是 models 单表 custom 全行读投影（旧
// provider_code/model 形状经别名保留，supported_endpoint_modes_json 承
// mode+protocols 物理来源）。
const customProviderModelColumns = `id, COALESCE(vendor_code, '') AS provider_code, public_model AS model, scope, system_account_id, status, catalog_visible,
	supported_endpoint_modes_json, supported_service_tiers_json,
	supported_reasoning_efforts_json, supported_tools_by_protocol_json, input_modalities_json, output_modalities_json, default_reasoning_effort,
	release_date, shutdown_date, context_window_tokens, max_input_tokens, max_output_tokens,
	input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m, cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
	image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
	output_usd_per_image, currency, pricing_notes, capability_notes, notes,
	created_by, updated_by, created_at, updated_at`

// findCustomProviderModelByID ports findCustomProviderModelByIdAsync; a
// non-empty ownerSystemAccountID adds the personal-owner predicate.
func (s *Store) findCustomProviderModelByID(ctx context.Context, id, ownerSystemAccountID string) (*customProviderModelRecord, error) {
	owner := strings.TrimSpace(ownerSystemAccountID)
	ownerPredicate := ""
	args := []any{id}
	if owner != "" {
		ownerPredicate = " AND scope = 'personal' AND system_account_id = ?"
		args = append(args, owner)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+customProviderModelColumns+`
		FROM `+s.table("models")+`
		WHERE id = ?`+ownerPredicate+`
		LIMIT 1`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanCustomProviderModelRecord(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		return record, rows.Err()
	}
	return nil, rows.Err()
}

// findCustomProviderModelByScope ports findCustomProviderModelByScopeAsync
// （身份键 (COALESCE(vendor_code,”), scope, COALESCE(system_account_id,”),
// upstream_model) 的等值读法）.
func (s *Store) findCustomProviderModelByScope(ctx context.Context, providerCode, scope, systemAccountID, model string) (*customProviderModelRecord, error) {
	query := ""
	var args []any
	if scope == catalogScopeGlobal {
		query = `SELECT ` + customProviderModelColumns + `
			FROM ` + s.table("models") + `
			WHERE vendor_code = ? AND scope = 'global' AND system_account_id IS NULL AND upstream_model = ?
			LIMIT 1`
		args = []any{providerCode, model}
	} else {
		query = `SELECT ` + customProviderModelColumns + `
			FROM ` + s.table("models") + `
			WHERE vendor_code = ? AND scope = 'personal' AND system_account_id = ? AND upstream_model = ?
			LIMIT 1`
		args = []any{providerCode, systemAccountID, model}
	}
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanCustomProviderModelRecord(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		return record, rows.Err()
	}
	return nil, rows.Err()
}

func scanCustomProviderModelRecord(scan func(...any) error) (*customProviderModelRecord, error) {
	var (
		record          customProviderModelRecord
		systemAccountID sql.NullString
		// models.catalog_visible 在 PostgreSQL 为 boolean、SQLite 为 integer，
		// 扫描目标用 NullBool 同时兼容两种驱动。
		catalogVisible                sql.NullBool
		endpointModes                 sql.NullString
		releaseDate, shutdownDate     sql.NullString
		tiers, efforts                sql.NullString
		toolsByProtocol               sql.NullString
		inputModalities               sql.NullString
		outputModalities              sql.NullString
		defaultEffort                 sql.NullString
		contextWindow, maxInput       sql.NullInt64
		maxOutput                     sql.NullInt64
		inputUsd, outputUsd           sql.NullFloat64
		cachedInput, cacheWrite       sql.NullFloat64
		cacheWrite1h, cacheStorage    sql.NullFloat64
		tierPrices                    sql.NullString
		imageIn, imageOut, audioIn    sql.NullFloat64
		audioOut, outputPerImage      sql.NullFloat64
		currency                      sql.NullString
		pricingNotes, capabilityNotes sql.NullString
		notes                         sql.NullString
		updatedBy                     sql.NullString
	)
	if err := scan(&record.ID, &record.ProviderCode, &record.Model, &record.Scope, &systemAccountID, &record.Status,
		&catalogVisible, &endpointModes, &tiers, &efforts, &toolsByProtocol, &inputModalities, &outputModalities, &defaultEffort,
		&releaseDate, &shutdownDate, &contextWindow, &maxInput, &maxOutput,
		&inputUsd, &outputUsd, &cachedInput, &cacheWrite, &cacheWrite1h, &cacheStorage, &tierPrices,
		&imageIn, &imageOut, &audioIn, &audioOut, &outputPerImage, &currency,
		&pricingNotes, &capabilityNotes, &notes,
		&record.CreatedBy, &updatedBy, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return nil, err
	}
	record.SystemAccountID = nullPtrString(systemAccountID)
	record.CatalogVisible = catalogVisible.Bool && catalogVisible.Valid
	// 旧 mode（四值 UI 分类）/ supportedApiProtocols 无物理列：由
	// supported_endpoint_modes_json 逆映射派生。
	record.Mode, record.SupportedAPIProtocols = deriveCustomModelProjection(endpointModes)
	record.SupportedServiceTiers = parseCapabilityTokenArray(tiers)
	record.SupportedReasoningEfforts = parseCapabilityTokenArray(efforts)
	// T82 能力声明三列 lenient 解析（口径与 scanCustomCatalogItem 同源）。
	record.SupportedToolsByProtocol = parseCapabilityTokenMatrix(toolsByProtocol)
	record.InputModalities = parseCapabilityModalityArray(inputModalities)
	record.OutputModalities = parseCapabilityModalityArray(outputModalities)
	record.DefaultReasoningEffort = capabilityTokenPtr(defaultEffort)
	record.ReleaseDate = textPtr(releaseDate)
	record.ShutdownDate = textPtr(shutdownDate)
	record.ContextWindowTokens = nullInt64Ptr(contextWindow)
	record.MaxInputTokens = nullInt64Ptr(maxInput)
	record.MaxOutputTokens = nullInt64Ptr(maxOutput)
	record.InputUsdPer1M = nullFloat64Ptr(inputUsd)
	record.OutputUsdPer1M = nullFloat64Ptr(outputUsd)
	record.CachedInputUsdPer1M = nullFloat64Ptr(cachedInput)
	record.CacheWriteUsdPer1M = nullFloat64Ptr(cacheWrite)
	record.CacheWrite1hUsdPer1M = nullFloat64Ptr(cacheWrite1h)
	record.CacheStorageUsdPer1MPerHour = nullFloat64Ptr(cacheStorage)
	record.ServiceTierPrices = normalizeServiceTierPrices(tierPrices)
	record.ImageInputUsdPer1M = nullFloat64Ptr(imageIn)
	record.ImageOutputUsdPer1M = nullFloat64Ptr(imageOut)
	record.AudioInputUsdPer1M = nullFloat64Ptr(audioIn)
	record.AudioOutputUsdPer1M = nullFloat64Ptr(audioOut)
	record.OutputUsdPerImage = nullFloat64Ptr(outputPerImage)
	record.PricingNotes = textPtr(pricingNotes)
	record.CapabilityNotes = textPtr(capabilityNotes)
	record.Notes = textPtr(notes)
	record.UpdatedBy = nullPtrString(updatedBy)
	return &record, nil
}

// deriveCustomModelProjection 把 supported_endpoint_modes_json 列值逆映射成自
// 定义模型的 (UI 分类 mode, supportedApiProtocols)：UI 分类按
// image > audio > video > text 收敛（写路径四值词表的读侧逆）；空 mode 集
// 不注入 mode、协议投影空数组。
func deriveCustomModelProjection(value sql.NullString) (*string, []string) {
	modes := parseJSONArray(value)
	_, protocols := DeriveLegacyModeAndProtocols(modes)
	if uiMode, ok := deriveCustomUIMode(modes); ok {
		return &uiMode, protocols
	}
	return nil, protocols
}

// deriveCustomUIMode 从 endpoint modes 反推自定义模型 UI 分类：images_json →
// image；音频族（audio_speech / audio_transcription_json / realtime_session /
// audio_job_*）→ audio；视频族 → video；其余（含空集）→ text。
func deriveCustomUIMode(endpointModes []string) (string, bool) {
	if len(endpointModes) == 0 {
		return "", false
	}
	has := func(targets ...string) bool {
		for _, mode := range endpointModes {
			for _, target := range targets {
				if mode == target {
					return true
				}
			}
		}
		return false
	}
	if has("images_json") {
		return "image", true
	}
	if has("audio_speech", "audio_transcription_json", "realtime_session",
		"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel") {
		return "audio", true
	}
	if has("video_create", "video_get", "video_content", "video_cancel") {
		return "video", true
	}
	return "text", true
}

// customModelBuiltInNameConflictMessage is the 2026-09-30 contract rejection
// text (自定义模型与模型映射设计.md 第 4 节：自定义模型不得与内置模型同名，
// 内置权威优先)；the routes render it as a 400 body.
const customModelBuiltInNameConflictMessage = "该模型已由系统内置提供，请直接使用系统模型"

// existsVisibleBuiltInCatalogModel reports whether the vendor's builtin 目录
// carries a runtime-visible row（scope='builtin'，active + catalog_visible +
// not shutdown）with the exact public_model name.
func (s *Store) existsVisibleBuiltInCatalogModel(ctx context.Context, providerCode, model string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT 1
		FROM `+s.table("models")+`
		WHERE vendor_code = ? AND public_model = ?
			AND scope = 'builtin'
			AND status = 'active'
			AND `+s.visibleTruePredicate("catalog_visible")+`
			AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+s.todayText()+`)
		LIMIT 1`), providerCode, model)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return true, rows.Err()
	}
	return false, rows.Err()
}

// upsertCustomProviderModel ports upsertCustomProviderModelAsync over the
// models 单表（identity 唯一索引 (COALESCE(vendor_code,”), scope,
// COALESCE(system_account_id,”), upstream_model)；id 保留旧 ON CONFLICT 门）。
// The returned error messages are verbatim repository texts; the route renders
// them as 400 bodies.
func (s *Store) upsertCustomProviderModel(ctx context.Context, input customProviderModelUpsertInput) (*customProviderModelRecord, error) {
	providerCode, err := requiredCustomText(input.ProviderCode, "供应商代码不能为空")
	if err != nil {
		return nil, err
	}
	model, err := requiredCustomText(input.Model, "模型 ID 不能为空")
	if err != nil {
		return nil, err
	}
	scope := catalogScopePersonal
	if input.Scope == catalogScopeGlobal {
		scope = catalogScopeGlobal
	}
	systemAccountID := ""
	if scope == catalogScopeGlobal {
		systemAccountID = ""
	} else {
		candidate := input.SystemAccountID
		if candidate == "" {
			candidate = input.ActorSystemAccountID
		}
		systemAccountID, err = requiredCustomText(candidate, "个人模型必须归属系统账户")
		if err != nil {
			return nil, err
		}
	}
	status := input.Status
	if status == "" {
		status = "active"
	}
	now := s.nowUTC().Format("2006-01-02T15:04:05.000Z07:00")
	var existing *customProviderModelRecord
	if input.ID != "" {
		existing, err = s.findCustomProviderModelByID(ctx, input.ID, "")
	} else {
		existing, err = s.findCustomProviderModelByScope(ctx, providerCode, scope, systemAccountID, model)
	}
	if err != nil {
		return nil, err
	}
	if existing != nil && strings.TrimSpace(existing.Model) != model {
		return nil, fmt.Errorf("模型 ID 创建后不能修改")
	}
	// 契约 2026-09-30（docs/functions/自定义模型与模型映射设计.md 第 4 节）：
	// 自定义模型不得与同供应商运行时可见的内置模型同名（内置权威优先），
	// 创建与整体保存全路径拒绝。
	builtInConflict, err := s.existsVisibleBuiltInCatalogModel(ctx, providerCode, model)
	if err != nil {
		return nil, err
	}
	if builtInConflict {
		return nil, fmt.Errorf("%s", customModelBuiltInNameConflictMessage)
	}
	id := ""
	if existing != nil {
		id = existing.ID
	} else if input.ID != "" {
		id = input.ID
	} else {
		id = newCustomModelID(s.nowUTC())
	}
	capabilities, err := normalizeCustomModelCapabilities(providerCode, customModelCapabilityInput{
		Mode:                      input.Mode,
		SupportedServiceTiers:     input.SupportedServiceTiers,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
		ServiceTierPrices:         input.ServiceTierPrices,
	})
	if err != nil {
		return nil, err
	}
	protocols := normalizeCustomProtocols(input.SupportedAPIProtocols)
	// supported_endpoint_modes_json：协议映射优先，协议为空回退 UI 分类缺省集
	//（未知协议 deny-by-default 拒绝）。
	mode := "text"
	if input.Mode != nil && strings.TrimSpace(*input.Mode) != "" {
		mode = strings.TrimSpace(*input.Mode)
	}
	endpointModes, err := ForwardCustomModelEndpointModes(mode, protocols)
	if err != nil {
		return nil, err
	}

	createdBy := input.ActorSystemAccountID
	createdAt := now
	if existing != nil {
		createdBy = existing.CreatedBy
		createdAt = existing.CreatedAt
	}
	args := []any{
		id, providerCode, model, model, scope, nullableTextValue(systemAccountID), status, s.boolValue(true),
		s.boolValue(input.CachedInputUsdPer1M != nil),
		mustJSON(endpointModes),
		mustJSON(capabilities.supportedServiceTiers), mustJSON(capabilities.supportedReasoningEfforts),
		capabilityMatrixJSON(input.SupportedToolsByProtocol), capabilityTokenArrayJSON(input.InputModalities), capabilityTokenArrayJSON(input.OutputModalities),
		nullableTextPtr(capabilities.defaultReasoningEffort),
		nullableDatePtr(input.ReleaseDate), nullableDatePtr(input.ShutdownDate),
		nullableInt64Ptr(input.ContextWindowTokens), nullableInt64Ptr(input.MaxInputTokens), nullableInt64Ptr(input.MaxOutputTokens),
		nullableFloat64Ptr(input.InputUsdPer1M), nullableFloat64Ptr(input.OutputUsdPer1M), nullableFloat64Ptr(input.CachedInputUsdPer1M),
		nullableFloat64Ptr(input.CacheWriteUsdPer1M), nullableFloat64Ptr(input.CacheWrite1hUsdPer1M), nullableFloat64Ptr(input.CacheStorageUsdPer1MPerHour),
		mustJSON(normalizeServiceTierPricesValue(input.ServiceTierPrices)),
		nullableFloat64Ptr(input.ImageInputUsdPer1M), nullableFloat64Ptr(input.ImageOutputUsdPer1M),
		nullableFloat64Ptr(input.AudioInputUsdPer1M), nullableFloat64Ptr(input.AudioOutputUsdPer1M),
		nullableFloat64Ptr(input.OutputUsdPerImage),
		// currency (Node inlines the 'USD' literal)
		"USD",
		nullableTextPtr(input.PricingNotes), nullableTextPtr(input.CapabilityNotes), nullableTextPtr(input.Notes),
		createdBy, input.ActorSystemAccountID, createdAt, now,
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("models")+` (
		id, vendor_code, public_model, upstream_model, scope, system_account_id, status, catalog_visible,
		supports_prompt_caching, supported_endpoint_modes_json, supported_service_tiers_json,
		supported_reasoning_efforts_json, supported_tools_by_protocol_json, input_modalities_json, output_modalities_json, default_reasoning_effort,
		release_date, shutdown_date, context_window_tokens, max_input_tokens, max_output_tokens,
		input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m, cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
		image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
		output_usd_per_image, currency, pricing_notes, capability_notes, notes,
		created_by, updated_by, created_at, updated_at
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		vendor_code = excluded.vendor_code,
		public_model = excluded.public_model,
		upstream_model = excluded.upstream_model,
		scope = excluded.scope,
		system_account_id = excluded.system_account_id,
		status = excluded.status,
		catalog_visible = excluded.catalog_visible,
		supports_prompt_caching = excluded.supports_prompt_caching,
		supported_endpoint_modes_json = excluded.supported_endpoint_modes_json,
		supported_service_tiers_json = excluded.supported_service_tiers_json,
		supported_reasoning_efforts_json = excluded.supported_reasoning_efforts_json,
		supported_tools_by_protocol_json = excluded.supported_tools_by_protocol_json,
		input_modalities_json = excluded.input_modalities_json,
		output_modalities_json = excluded.output_modalities_json,
		default_reasoning_effort = excluded.default_reasoning_effort,
		release_date = excluded.release_date,
		shutdown_date = excluded.shutdown_date,
		context_window_tokens = excluded.context_window_tokens,
		max_input_tokens = excluded.max_input_tokens,
		max_output_tokens = excluded.max_output_tokens,
		input_usd_per_1m = excluded.input_usd_per_1m,
		output_usd_per_1m = excluded.output_usd_per_1m,
		cached_input_usd_per_1m = excluded.cached_input_usd_per_1m,
		cache_write_usd_per_1m = excluded.cache_write_usd_per_1m,
		cache_write_1h_usd_per_1m = excluded.cache_write_1h_usd_per_1m,
		cache_storage_usd_per_1m_per_hour = excluded.cache_storage_usd_per_1m_per_hour,
		service_tier_prices_json = excluded.service_tier_prices_json,
		image_input_usd_per_1m = excluded.image_input_usd_per_1m,
		image_output_usd_per_1m = excluded.image_output_usd_per_1m,
		audio_input_usd_per_1m = excluded.audio_input_usd_per_1m,
		audio_output_usd_per_1m = excluded.audio_output_usd_per_1m,
		output_usd_per_image = excluded.output_usd_per_image,
		pricing_notes = excluded.pricing_notes,
		capability_notes = excluded.capability_notes,
		notes = excluded.notes,
		updated_by = excluded.updated_by,
		updated_at = excluded.updated_at`), args...)
	if err != nil {
		return nil, err
	}
	saved, err := s.findCustomProviderModelByID(ctx, id, "")
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, fmt.Errorf("自定义模型保存失败")
	}
	return saved, nil
}

// patchCustomProviderModel ports patchCustomProviderModelAsync: the
// optimistic-concurrency UPDATE guarded by updated_at plus the in-transaction
// default-reference cleanup. fields carries the submitted patch keys.
func (s *Store) patchCustomProviderModel(ctx context.Context, current *customProviderModelRecord, next customProviderModelUpsertInput,
	fields []string, expectedUpdatedAt, ownerSystemAccountID string, cleanup *defaultReferenceCleanupInput) (*customModelPatchOutcome, error) {
	// 契约 2026-09-30（docs/functions/自定义模型与模型映射设计.md 第 4 节）：
	// 改名（提交了与现名不同的非空 model）撞同供应商运行时可见内置模型时拒绝；
	// 名字未变（含 HTTP patch 不携带 model 键的零值）不查询，正常编辑不受影响。
	if renamed := strings.TrimSpace(next.Model); renamed != "" && renamed != strings.TrimSpace(current.Model) {
		builtInConflict, err := s.existsVisibleBuiltInCatalogModel(ctx, current.ProviderCode, renamed)
		if err != nil {
			return nil, err
		}
		if builtInConflict {
			return nil, fmt.Errorf("%s", customModelBuiltInNameConflictMessage)
		}
	}
	if current.UpdatedAt != expectedUpdatedAt {
		return &customModelPatchOutcome{Kind: "conflict", Record: customModelMutationRecordOf(current)}, nil
	}
	assignments, params, merged, err := s.customProviderModelPatchAssignments(current, next, fields)
	if err != nil {
		return nil, err
	}
	if len(assignments) == 0 {
		return &customModelPatchOutcome{Kind: "no_op", Record: customModelMutationRecordOf(current)}, nil
	}
	updatedAt := nextCustomModelUpdatedAt(current.UpdatedAt, s.nowUTC())
	writeParams := append([]any{}, params...)
	writeParams = append(writeParams, next.ActorSystemAccountID, updatedAt, current.ID, expectedUpdatedAt)
	owner := strings.TrimSpace(ownerSystemAccountID)
	ownerPredicate := ""
	if owner != "" {
		ownerPredicate = " AND scope = 'personal' AND system_account_id = ?"
		writeParams = append(writeParams, owner)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	update := s.bind(`UPDATE ` + s.table("models") + `
		SET ` + strings.Join(assignments, ", ") + `, updated_by = ?, updated_at = ?
		WHERE id = ? AND updated_at = ?` + ownerPredicate)
	result, err := tx.ExecContext(ctx, update, writeParams...)
	if err != nil {
		return nil, err
	}
	changes, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	clearedProviderCodes := []string{}
	if changes == 0 {
		return &customModelPatchOutcome{Kind: "conflict", Record: customModelMutationRecordOf(current)}, nil
	}
	if cleanup != nil {
		clearedProviderCodes, err = clearUnavailableProviderModelDefaultReferences(ctx, tx, s, cleanup)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	merged.UpdatedAt = updatedAt
	merged.UpdatedBy = stringPtr(next.ActorSystemAccountID)
	return &customModelPatchOutcome{
		Kind:                                   "updated",
		Record:                                 customModelMutationRecordOf(&merged),
		ClearedDefaultHealthCheckProviderCodes: clearedProviderCodes,
	}, nil
}

// deleteCustomProviderModel ports deleteCustomProviderModelAsync: the
// owner-guarded delete plus the in-transaction default-reference cleanup.
func (s *Store) deleteCustomProviderModel(ctx context.Context, id, ownerSystemAccountID string, cleanup *defaultReferenceCleanupInput) (bool, error) {
	owner := strings.TrimSpace(ownerSystemAccountID)
	ownerPredicate := ""
	args := []any{id}
	if owner != "" {
		ownerPredicate = " AND scope = 'personal' AND system_account_id = ?"
		args = append(args, owner)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// 死账户悬挂引用清理（BUG-0336，外键口径对齐）：account_supported_models
	// 的 model_id 外键为 RESTRICT，不区分引用账户是否存活；绑定守卫只统计活
	// 账户（活引用在 handler 以 409 语义拒绝），软删/缺失账户的悬挂引用行会让
	// DELETE models 以 23503 兜底成 500（2026-10-22 重建回迁的软删账户数据首次
	// 暴露）。删除前在同事务先清 entry_mappings（复合外键指向 supported_models，
	// 先解引用再删被引用行；source_model 为模型名文本键，同口径一并清理），
	// 再清 supported_models；NOT EXISTS 同时覆盖软删账户与孤儿行。
	deadRefSQL := `DELETE FROM %s WHERE %s AND NOT EXISTS (
		SELECT 1 FROM ` + s.table("accounts") + ` accounts
		WHERE accounts.id = %s AND accounts.deleted_at IS NULL)`
	deadRefStatements := []struct {
		table     string
		predicate string
		identity  string
	}{
		{table: s.table("account_entry_mappings"), predicate: "target_model_id = ?", identity: "account_entry_mappings.account_id"},
		{table: s.table("account_entry_mappings"), predicate: "source_model = (SELECT upstream_model FROM " + s.table("models") + " WHERE id = ?)", identity: "account_entry_mappings.account_id"},
		{table: s.table("account_supported_models"), predicate: "model_id = ?", identity: "account_supported_models.account_id"},
	}
	for _, statement := range deadRefStatements {
		if _, err := tx.ExecContext(ctx, s.bind(fmt.Sprintf(deadRefSQL, statement.table, statement.predicate, statement.identity)), id); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("models")+`
		WHERE id = ?`+ownerPredicate), args...)
	if err != nil {
		return false, err
	}
	changes, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changes > 0 && cleanup != nil {
		if _, err := clearUnavailableProviderModelDefaultReferences(ctx, tx, s, cleanup); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changes > 0, nil
}

// customProviderModelBindingSummary mirrors CustomProviderModelAccountBindingSummary.
type customProviderModelBindingSummary struct {
	SupportedModelAccountCount  int64
	MappingSourceAccountCount   int64
	MappingUpstreamAccountCount int64
	TotalAccountCount           int64
}

// customProviderModelBindings ports customProviderModelAccountBindingSummaryAsync
// over the target relation shapes：支持模型关系 (account_id, model_id) 直接引用
// models.id；入口映射 account_entry_mappings 以 source_model（public name）与
// target_model_id 计两个映射向（旧 account_model_mappings 的
// source_model/upstream_model 双列随表退役）。
func (s *Store) customProviderModelBindings(ctx context.Context, providerCode, model, scope, systemAccountID string) (*customProviderModelBindingSummary, error) {
	providerCode, err := requiredCustomText(providerCode, "供应商代码不能为空")
	if err != nil {
		return nil, err
	}
	model, err = requiredCustomText(model, "模型 ID 不能为空")
	if err != nil {
		return nil, err
	}
	ownerPredicate := ""
	ownerArgs := []any{}
	if scope != catalogScopeGlobal {
		owner, err := requiredCustomText(systemAccountID, "个人模型必须归属系统账户")
		if err != nil {
			return nil, err
		}
		ownerPredicate = " AND accounts.system_account_id = ?"
		ownerArgs = []any{owner}
	}
	accounts := s.table("accounts")
	supportedModels := s.table("account_supported_models")
	entryMappings := s.table("account_entry_mappings")
	supportedModelSQL := `SELECT account_supported_models.account_id
		FROM ` + supportedModels + ` account_supported_models
		INNER JOIN ` + accounts + ` accounts
			ON accounts.id = account_supported_models.account_id
			AND accounts.deleted_at IS NULL
		WHERE account_supported_models.model_id = ?
		` + ownerPredicate
	mappingSourceSQL := `SELECT account_entry_mappings.account_id
		FROM ` + entryMappings + ` account_entry_mappings
		INNER JOIN ` + accounts + ` accounts
			ON accounts.id = account_entry_mappings.account_id
			AND accounts.deleted_at IS NULL
		WHERE account_entry_mappings.source_model = ?
		` + ownerPredicate
	mappingUpstreamSQL := `SELECT account_entry_mappings.account_id
		FROM ` + entryMappings + ` account_entry_mappings
		INNER JOIN ` + accounts + ` accounts
			ON accounts.id = account_entry_mappings.account_id
			AND accounts.deleted_at IS NULL
		WHERE account_entry_mappings.target_model_id = ?
		` + ownerPredicate

	// 支持模型关系按 models.id 计数：身份键定位当前行的物理 id（行缺失时
	// 三个计数恒 0，守卫语义保持“无绑定可删”）。
	modelID := ""
	idQuery := `SELECT id FROM ` + s.table("models") + `
		WHERE vendor_code = ? AND upstream_model = ? AND scope = ?`
	idArgs := []any{providerCode, model, scope}
	if scope == catalogScopeGlobal {
		idQuery += " AND system_account_id IS NULL LIMIT 1"
	} else {
		idQuery += " AND system_account_id = ? LIMIT 1"
		idArgs = append(idArgs, strings.TrimSpace(systemAccountID))
	}
	if err := s.db.QueryRowContext(ctx, s.bind(idQuery), idArgs...).Scan(&modelID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		modelID = ""
	}
	supportedModelArgs := append([]any{modelID}, ownerArgs...)
	mappingSourceArgs := append([]any{model}, ownerArgs...)
	mappingUpstreamArgs := append([]any{modelID}, ownerArgs...)
	_ = providerCode

	summary := &customProviderModelBindingSummary{}
	if summary.SupportedModelAccountCount, err = s.countDistinctBoundAccounts(ctx,
		`SELECT account_id FROM (`+supportedModelSQL+`) bound_accounts`, supportedModelArgs); err != nil {
		return nil, err
	}
	if summary.MappingSourceAccountCount, err = s.countDistinctBoundAccounts(ctx,
		`SELECT account_id FROM (`+mappingSourceSQL+`) bound_accounts`, mappingSourceArgs); err != nil {
		return nil, err
	}
	if summary.MappingUpstreamAccountCount, err = s.countDistinctBoundAccounts(ctx,
		`SELECT account_id FROM (`+mappingUpstreamSQL+`) bound_accounts`, mappingUpstreamArgs); err != nil {
		return nil, err
	}
	unionArgs := append(append(append([]any{}, supportedModelArgs...), mappingSourceArgs...), mappingUpstreamArgs...)
	if summary.TotalAccountCount, err = s.countDistinctBoundAccounts(ctx,
		`SELECT account_id FROM (
			`+supportedModelSQL+`
			UNION
			`+mappingSourceSQL+`
			UNION
			`+mappingUpstreamSQL+`
		) bound_account_union`, unionArgs); err != nil {
		return nil, err
	}
	return summary, nil
}

func (s *Store) countDistinctBoundAccounts(ctx context.Context, query string, args []any) (int64, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT COUNT(DISTINCT account_id) AS count FROM (`+query+`)`), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var count int64
		if err := rows.Scan(&count); err != nil {
			return 0, err
		}
		return count, rows.Err()
	}
	return 0, rows.Err()
}

// customProviderModelPatchAssignments ports customProviderModelPatchAssignments:
// only submitted fields participate, equal values are dropped (JSON string
// equality) and the capability columns re-normalize over the merged next.
// mode 与 supportedApiProtocols 合写同一物理列 supported_endpoint_modes_json：
// 两键任一提交即按合并后的 (UI mode, protocols) 重算 modes json。
func (s *Store) customProviderModelPatchAssignments(current *customProviderModelRecord, next customProviderModelUpsertInput,
	fields []string) ([]string, []any, customProviderModelRecord, error) {
	requested := map[string]bool{}
	for _, field := range fields {
		requested[field] = true
	}
	merged := *current
	assignments := []string{}
	params := []any{}
	add := func(field, column string, nextValue any, currentValue any, apply func()) {
		if !requested[field] || patchValuesEqual(nextValue, currentValue) {
			return
		}
		assignments = append(assignments, column+" = ?")
		params = append(params, nextValue)
		if apply != nil {
			apply()
		}
	}
	capabilities, err := normalizeCustomModelCapabilities(current.ProviderCode, customModelCapabilityInput{
		Mode:                      next.Mode,
		SupportedServiceTiers:     next.SupportedServiceTiers,
		SupportedReasoningEfforts: next.SupportedReasoningEfforts,
		ServiceTierPrices:         next.ServiceTierPrices,
	})
	if err != nil {
		return nil, nil, merged, err
	}
	status := next.Status
	if status == "" {
		status = current.Status
	}
	add("status", "status", status, current.Status, func() { merged.Status = status })
	// mode / supportedApiProtocols → supported_endpoint_modes_json 合写。
	if requested["mode"] || requested["supportedApiProtocols"] {
		mergedMode := "text"
		if merged.Mode != nil && strings.TrimSpace(*merged.Mode) != "" {
			mergedMode = strings.TrimSpace(*merged.Mode)
		}
		mergedProtocols := normalizeCustomProtocols(merged.SupportedAPIProtocols)
		mergedModes, modesErr := ForwardCustomModelEndpointModes(mergedMode, mergedProtocols)
		if modesErr != nil {
			return nil, nil, merged, modesErr
		}
		currentModes, _, currentProjectionErr := decodeCurrentEndpointModes(current)
		if currentProjectionErr != nil {
			return nil, nil, merged, currentProjectionErr
		}
		if !patchValuesEqual(mustJSON(mergedModes), mustJSON(currentModes)) {
			assignments = append(assignments, "supported_endpoint_modes_json = ?")
			params = append(params, mustJSON(mergedModes))
			merged.Mode, merged.SupportedAPIProtocols = deriveCustomModelProjection(sql.NullString{String: mustJSON(mergedModes), Valid: true})
		}
	}
	add("supportedServiceTiers", "supported_service_tiers_json",
		mustJSON(capabilities.supportedServiceTiers), mustJSON(current.SupportedServiceTiers),
		func() { merged.SupportedServiceTiers = capabilities.supportedServiceTiers })
	add("supportedReasoningEfforts", "supported_reasoning_efforts_json",
		mustJSON(capabilities.supportedReasoningEfforts), mustJSON(current.SupportedReasoningEfforts),
		func() { merged.SupportedReasoningEfforts = capabilities.supportedReasoningEfforts })
	// T82 显式能力声明三列：提交键整值替换（空 object/array = 清空 = 未声明），
	// 缺键保持现值（沿 parsed.present 机制）；nil 归一空值满足列 CHECK。
	add("supportedToolsByProtocol", "supported_tools_by_protocol_json",
		capabilityMatrixJSON(next.SupportedToolsByProtocol), capabilityMatrixJSON(current.SupportedToolsByProtocol),
		func() { merged.SupportedToolsByProtocol = next.SupportedToolsByProtocol })
	add("inputModalities", "input_modalities_json",
		capabilityTokenArrayJSON(next.InputModalities), capabilityTokenArrayJSON(current.InputModalities),
		func() { merged.InputModalities = next.InputModalities })
	add("outputModalities", "output_modalities_json",
		capabilityTokenArrayJSON(next.OutputModalities), capabilityTokenArrayJSON(current.OutputModalities),
		func() { merged.OutputModalities = next.OutputModalities })
	add("defaultReasoningEffort", "default_reasoning_effort",
		nullableTextPtr(capabilities.defaultReasoningEffort), nullableTextPtr(current.DefaultReasoningEffort),
		func() { merged.DefaultReasoningEffort = capabilities.defaultReasoningEffort })
	add("releaseDate", "release_date", nullableDatePtr(next.ReleaseDate), nullableDatePtr(current.ReleaseDate),
		func() { merged.ReleaseDate = normalizedDateCopy(next.ReleaseDate) })
	add("shutdownDate", "shutdown_date", nullableDatePtr(next.ShutdownDate), nullableDatePtr(current.ShutdownDate),
		func() { merged.ShutdownDate = normalizedDateCopy(next.ShutdownDate) })
	add("contextWindowTokens", "context_window_tokens", nullableInt64Ptr(next.ContextWindowTokens), nullableInt64Ptr(current.ContextWindowTokens),
		func() { merged.ContextWindowTokens = next.ContextWindowTokens })
	add("maxInputTokens", "max_input_tokens", nullableInt64Ptr(next.MaxInputTokens), nullableInt64Ptr(current.MaxInputTokens),
		func() { merged.MaxInputTokens = next.MaxInputTokens })
	add("maxOutputTokens", "max_output_tokens", nullableInt64Ptr(next.MaxOutputTokens), nullableInt64Ptr(current.MaxOutputTokens),
		func() { merged.MaxOutputTokens = next.MaxOutputTokens })
	// cached-input 价变更联动 supports_prompt_caching（读侧直接读列，见
	// scanCustomCatalogItem；价未提交或未变化时列保持现值）。
	if requested["cachedInputUsdPer1M"] && !patchValuesEqual(nullableFloat64Ptr(next.CachedInputUsdPer1M), nullableFloat64Ptr(current.CachedInputUsdPer1M)) {
		assignments = append(assignments, "supports_prompt_caching = ?")
		params = append(params, s.boolValue(next.CachedInputUsdPer1M != nil))
	}
	add("cachedInputUsdPer1M", "cached_input_usd_per_1m", nullableFloat64Ptr(next.CachedInputUsdPer1M), nullableFloat64Ptr(current.CachedInputUsdPer1M),
		func() { merged.CachedInputUsdPer1M = next.CachedInputUsdPer1M })
	add("inputUsdPer1M", "input_usd_per_1m", nullableFloat64Ptr(next.InputUsdPer1M), nullableFloat64Ptr(current.InputUsdPer1M),
		func() { merged.InputUsdPer1M = next.InputUsdPer1M })
	add("outputUsdPer1M", "output_usd_per_1m", nullableFloat64Ptr(next.OutputUsdPer1M), nullableFloat64Ptr(current.OutputUsdPer1M),
		func() { merged.OutputUsdPer1M = next.OutputUsdPer1M })
	add("cacheWriteUsdPer1M", "cache_write_usd_per_1m", nullableFloat64Ptr(next.CacheWriteUsdPer1M), nullableFloat64Ptr(current.CacheWriteUsdPer1M),
		func() { merged.CacheWriteUsdPer1M = next.CacheWriteUsdPer1M })
	add("cacheWrite1hUsdPer1M", "cache_write_1h_usd_per_1m", nullableFloat64Ptr(next.CacheWrite1hUsdPer1M), nullableFloat64Ptr(current.CacheWrite1hUsdPer1M),
		func() { merged.CacheWrite1hUsdPer1M = next.CacheWrite1hUsdPer1M })
	add("cacheStorageUsdPer1MPerHour", "cache_storage_usd_per_1m_per_hour", nullableFloat64Ptr(next.CacheStorageUsdPer1MPerHour), nullableFloat64Ptr(current.CacheStorageUsdPer1MPerHour),
		func() { merged.CacheStorageUsdPer1MPerHour = next.CacheStorageUsdPer1MPerHour })
	add("serviceTierPrices", "service_tier_prices_json",
		mustJSON(normalizeServiceTierPricesValue(next.ServiceTierPrices)),
		mustJSON(normalizeServiceTierPricesValue(current.ServiceTierPrices)),
		func() { merged.ServiceTierPrices = normalizeServiceTierPricesValue(next.ServiceTierPrices) })
	add("imageInputUsdPer1M", "image_input_usd_per_1m", nullableFloat64Ptr(next.ImageInputUsdPer1M), nullableFloat64Ptr(current.ImageInputUsdPer1M),
		func() { merged.ImageInputUsdPer1M = next.ImageInputUsdPer1M })
	add("imageOutputUsdPer1M", "image_output_usd_per_1m", nullableFloat64Ptr(next.ImageOutputUsdPer1M), nullableFloat64Ptr(current.ImageOutputUsdPer1M),
		func() { merged.ImageOutputUsdPer1M = next.ImageOutputUsdPer1M })
	add("audioInputUsdPer1M", "audio_input_usd_per_1m", nullableFloat64Ptr(next.AudioInputUsdPer1M), nullableFloat64Ptr(current.AudioInputUsdPer1M),
		func() { merged.AudioInputUsdPer1M = next.AudioInputUsdPer1M })
	add("audioOutputUsdPer1M", "audio_output_usd_per_1m", nullableFloat64Ptr(next.AudioOutputUsdPer1M), nullableFloat64Ptr(current.AudioOutputUsdPer1M),
		func() { merged.AudioOutputUsdPer1M = next.AudioOutputUsdPer1M })
	add("outputUsdPerImage", "output_usd_per_image", nullableFloat64Ptr(next.OutputUsdPerImage), nullableFloat64Ptr(current.OutputUsdPerImage),
		func() { merged.OutputUsdPerImage = next.OutputUsdPerImage })
	add("pricingNotes", "pricing_notes", nullableTextPtr(next.PricingNotes), nullableTextPtr(current.PricingNotes),
		func() { merged.PricingNotes = next.PricingNotes })
	add("capabilityNotes", "capability_notes", nullableTextPtr(next.CapabilityNotes), nullableTextPtr(current.CapabilityNotes),
		func() { merged.CapabilityNotes = next.CapabilityNotes })
	add("notes", "notes", nullableTextPtr(next.Notes), nullableTextPtr(current.Notes),
		func() { merged.Notes = next.Notes })
	return assignments, params, merged, nil
}

// decodeCurrentEndpointModes 是 patch 合写时的当前行 modes 读取：当前行无独立
// modes 缓存，从派生的 (UI mode, protocols) 正向重建（与写路径同一投影，往返
// 稳定）。
func decodeCurrentEndpointModes(current *customProviderModelRecord) ([]string, string, error) {
	mode := "text"
	if current.Mode != nil && strings.TrimSpace(*current.Mode) != "" {
		mode = strings.TrimSpace(*current.Mode)
	}
	modes, err := ForwardCustomModelEndpointModes(mode, normalizeCustomProtocols(current.SupportedAPIProtocols))
	if err != nil {
		return nil, mode, err
	}
	return modes, mode, nil
}

func customModelMutationRecordOf(record *customProviderModelRecord) customProviderModelMutationRecord {
	return customProviderModelMutationRecord{
		ID:              record.ID,
		ProviderCode:    record.ProviderCode,
		Model:           record.Model,
		Scope:           record.Scope,
		SystemAccountID: record.SystemAccountID,
		Status:          record.Status,
		CatalogVisible:  record.CatalogVisible,
		ShutdownDate:    record.ShutdownDate,
		UpdatedAt:       record.UpdatedAt,
	}
}

// --- normalization helpers (write side) ----------------------------------

var customProviderModelProtocolSet = map[string]bool{
	"chat_completions":        true,
	"responses":               true,
	"messages":                true,
	"message_token_counting":  true,
	"generate_content":        true,
	"stream_generate_content": true,
	"count_tokens":            true,
	"embed_content":           true,
	"interactions":            true,
	"completions":             true,
	"images":                  true,
	// M1 同步音频协议词表（设计 §11.3）；M2 视频协议 video（设计 §11.3）；
	// realtime 随 M5b realtime 基建放开（Realtime 设计 §6；不进映射面）。
	"audio_speech":        true,
	"audio_transcription": true,
	"video":               true,
	"realtime":            true,
}

var customModelCapabilityTokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// providerModelToolTokens 是 custom 模型显式工具能力声明（T82
// supportedToolsByProtocol 值域）的固定工具词表：写侧唯一入口，读侧不做词表
// 过滤。
var providerModelToolTokens = map[string]bool{
	"function_calling":        true,
	"web_search":              true,
	"google_search_grounding": true,
	"google_maps_grounding":   true,
	"file_search":             true,
	"image_generation":        true,
	"code_interpreter":        true,
	"code_execution":          true,
	"hosted_shell":            true,
	"apply_patch":             true,
	"skills":                  true,
	"computer_use":            true,
	"mcp":                     true,
	"tool_search":             true,
	"structured_outputs":      true,
	"url_context":             true,
}

// providerModelModalityTokens 是 custom 模型显式模态声明（T82 input/output
// modalities 值域）的固定词表：写侧唯一入口。
var providerModelModalityTokens = map[string]bool{
	"text":  true,
	"image": true,
	"audio": true,
	"video": true,
	"file":  true,
}

// normalizeCustomProtocols mirrors normalizeProtocols: trim, filter to the
// known protocol set, dedupe, order preserved.
func normalizeCustomProtocols(values []string) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if !customProviderModelProtocolSet[trimmed] || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		output = append(output, trimmed)
	}
	return output
}

type customModelCapabilityInput struct {
	Mode                      *string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	ServiceTierPrices         map[string]ModelPriceSet
}

type normalizedCustomModelCapabilities struct {
	supportedServiceTiers     []string
	supportedReasoningEfforts []string
	defaultReasoningEffort    *string
}

// normalizeCustomModelCapabilities mirrors normalizeCustomProviderModelCapabilities
// including its verbatim error texts.
func normalizeCustomModelCapabilities(providerCode string, input customModelCapabilityInput) (*normalizedCustomModelCapabilities, error) {
	mode := "text"
	if input.Mode != nil && strings.TrimSpace(*input.Mode) != "" {
		mode = strings.TrimSpace(*input.Mode)
	}
	if mode != "text" && mode != "image" && mode != "audio" && mode != "video" {
		return nil, fmt.Errorf("当前只支持文本、图像、音频和视频自定义模型")
	}
	supportedServiceTiers, err := normalizeCapabilityTokenArray(input.SupportedServiceTiers, "服务等级")
	if err != nil {
		return nil, err
	}
	supportedReasoningEfforts, err := normalizeCapabilityTokenArray(input.SupportedReasoningEfforts, "思考级别")
	if err != nil {
		return nil, err
	}
	if normalizeProviderToken(providerCode) == "gpt" {
		// 值域来自 shared/platform/requestoverride 单一真源（T19）：自定义模型
		// 能力不声明 `default` 服务等级（default 是运行时“强制标准档”指令）。
		gptServiceTiers := requestoverride.ServiceTierSetWithout(requestoverride.FamilyOpenAI, "default")
		gptReasoningEfforts := requestoverride.ReasoningEffortSet(requestoverride.FamilyOpenAI)
		if len(input.SupportedServiceTiers) > len(gptServiceTiers) || len(input.SupportedReasoningEfforts) > len(gptReasoningEfforts) {
			return nil, fmt.Errorf("自定义模型参数无效")
		}
		for _, value := range supportedServiceTiers {
			if !gptServiceTiers[value] {
				return nil, fmt.Errorf("自定义模型参数无效")
			}
		}
		for _, value := range supportedReasoningEfforts {
			if !gptReasoningEfforts[value] {
				return nil, fmt.Errorf("自定义模型参数无效")
			}
		}
	}
	isTextModel := mode == "text"
	serviceTierPriceKeys := sortedTierKeys(normalizeServiceTierPricesValue(input.ServiceTierPrices))
	if !isTextModel && (len(supportedServiceTiers) > 0 || len(supportedReasoningEfforts) > 0) {
		return nil, fmt.Errorf("只有文本自定义模型支持服务等级和思考能力配置")
	}
	if !isTextModel && len(serviceTierPriceKeys) > 0 {
		return nil, fmt.Errorf("只有文本自定义模型支持服务档位价格")
	}
	tierSet := map[string]bool{}
	for _, tier := range supportedServiceTiers {
		tierSet[tier] = true
	}
	for _, tier := range serviceTierPriceKeys {
		if !tierSet[tier] {
			return nil, fmt.Errorf("服务档位价格必须属于模型支持的服务等级")
		}
	}
	return &normalizedCustomModelCapabilities{
		supportedServiceTiers:     supportedServiceTiers,
		supportedReasoningEfforts: supportedReasoningEfforts,
		defaultReasoningEffort:    nil,
	}, nil
}

// normalizeCapabilityTokenArray mirrors normalizeEnumArray with the
// capability-token allowed set.
func normalizeCapabilityTokenArray(values []string, label string) ([]string, error) {
	output := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if !customModelCapabilityTokenPattern.MatchString(value) {
			shown := value
			if shown == "" {
				shown = raw
			}
			return nil, fmt.Errorf("%s包含不支持的值：%s", label, shown)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		output = append(output, value)
	}
	return output, nil
}

// parseCapabilityTokenArray mirrors parseEnumArray (read side): invalid or
// unknown entries are dropped silently.
func parseCapabilityTokenArray(value sql.NullString) []string {
	output := []string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return output
	}
	var raw []any
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return output
	}
	for _, item := range raw {
		text, ok := item.(string)
		if !ok || !customModelCapabilityTokenPattern.MatchString(text) {
			continue
		}
		output = append(output, text)
	}
	return output
}

// parseCapabilityTokenMatrix 解析显式工具能力矩阵（T82 supported_tools_by_protocol_json
// 读侧 lenient 口径）：JSON object，键=协议 token、值=token 数组；键或值 token 不匹配
// customModelCapabilityTokenPattern 的静默丢弃，值非数组的键整键丢弃，解析失败或
// 空 → 空矩阵（=未声明）。读侧不做词表白名单过滤（写侧是唯一入口）。
func parseCapabilityTokenMatrix(value sql.NullString) map[string][]string {
	output := map[string][]string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return output
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return output
	}
	for rawProtocol, rawTokens := range raw {
		protocol := strings.TrimSpace(rawProtocol)
		if !customModelCapabilityTokenPattern.MatchString(protocol) {
			continue
		}
		var items []any
		if err := json.Unmarshal(rawTokens, &items); err != nil {
			continue
		}
		tokens := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok || !customModelCapabilityTokenPattern.MatchString(text) {
				continue
			}
			tokens = append(tokens, text)
		}
		output[protocol] = tokens
	}
	return output
}

// parseCapabilityModalityArray 解析显式模态声明（T82 input/output_modalities_json
// 读侧 lenient 口径）：JSON array，token 不匹配 customModelCapabilityTokenPattern
// 的静默丢弃，解析失败或空 → 空数组。读侧不做词表白名单过滤（写侧是唯一入口）。
func parseCapabilityModalityArray(value sql.NullString) []string {
	output := []string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return output
	}
	var raw []any
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return output
	}
	for _, item := range raw {
		text, ok := item.(string)
		if !ok || !customModelCapabilityTokenPattern.MatchString(text) {
			continue
		}
		output = append(output, text)
	}
	return output
}

func capabilityTokenPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	trimmed := strings.TrimSpace(value.String)
	if trimmed == "" || !customModelCapabilityTokenPattern.MatchString(trimmed) {
		return nil
	}
	return &trimmed
}

// normalizeServiceTierPricesValue ports the write-side normalizeServiceTierPrices:
// unknown tiers and empty price sets are dropped, key order normalized.
func normalizeServiceTierPricesValue(value map[string]ModelPriceSet) map[string]ModelPriceSet {
	result := map[string]ModelPriceSet{}
	for rawTier, prices := range value {
		tier := strings.TrimSpace(rawTier)
		if tier == "" || tier == "default" || tier == "standard" || len(tier) > 64 {
			continue
		}
		normalized := ModelPriceSet{}
		if prices.InputUsdPer1M != nil && *prices.InputUsdPer1M >= 0 {
			normalized.InputUsdPer1M = prices.InputUsdPer1M
		}
		if prices.OutputUsdPer1M != nil && *prices.OutputUsdPer1M >= 0 {
			normalized.OutputUsdPer1M = prices.OutputUsdPer1M
		}
		if prices.CachedInputUsdPer1M != nil && *prices.CachedInputUsdPer1M >= 0 {
			normalized.CachedInputUsdPer1M = prices.CachedInputUsdPer1M
		}
		if prices.CacheWriteUsdPer1M != nil && *prices.CacheWriteUsdPer1M >= 0 {
			normalized.CacheWriteUsdPer1M = prices.CacheWriteUsdPer1M
		}
		if prices.CacheWrite1hUsdPer1M != nil && *prices.CacheWrite1hUsdPer1M >= 0 {
			normalized.CacheWrite1hUsdPer1M = prices.CacheWrite1hUsdPer1M
		}
		if prices.CacheStorageUsdPer1MPerHour != nil && *prices.CacheStorageUsdPer1MPerHour >= 0 {
			normalized.CacheStorageUsdPer1MPerHour = prices.CacheStorageUsdPer1MPerHour
		}
		if prices.ImageInputUsdPer1M != nil && *prices.ImageInputUsdPer1M >= 0 {
			normalized.ImageInputUsdPer1M = prices.ImageInputUsdPer1M
		}
		if prices.ImageOutputUsdPer1M != nil && *prices.ImageOutputUsdPer1M >= 0 {
			normalized.ImageOutputUsdPer1M = prices.ImageOutputUsdPer1M
		}
		if prices.AudioInputUsdPer1M != nil && *prices.AudioInputUsdPer1M >= 0 {
			normalized.AudioInputUsdPer1M = prices.AudioInputUsdPer1M
		}
		if prices.AudioOutputUsdPer1M != nil && *prices.AudioOutputUsdPer1M >= 0 {
			normalized.AudioOutputUsdPer1M = prices.AudioOutputUsdPer1M
		}
		if prices.OutputUsdPerImage != nil && *prices.OutputUsdPerImage >= 0 {
			normalized.OutputUsdPerImage = prices.OutputUsdPerImage
		}
		if modelPriceSetDefined(normalized) {
			result[tier] = normalized
		}
	}
	return result
}

func modelPriceSetDefined(set ModelPriceSet) bool {
	return set.InputUsdPer1M != nil || set.OutputUsdPer1M != nil || set.CachedInputUsdPer1M != nil ||
		set.CacheWriteUsdPer1M != nil || set.CacheWrite1hUsdPer1M != nil || set.CacheStorageUsdPer1MPerHour != nil ||
		set.ImageInputUsdPer1M != nil || set.ImageOutputUsdPer1M != nil || set.AudioInputUsdPer1M != nil ||
		set.AudioOutputUsdPer1M != nil || set.OutputUsdPerImage != nil
}

func sortedTierKeys(prices map[string]ModelPriceSet) []string {
	keys := make([]string, 0, len(prices))
	for tier := range prices {
		keys = append(keys, tier)
	}
	for index := 1; index < len(keys); index++ {
		for position := index; position > 0 && keys[position] < keys[position-1]; position-- {
			keys[position], keys[position-1] = keys[position-1], keys[position]
		}
	}
	return keys
}

func requiredCustomText(value, message string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s", message)
	}
	return trimmed, nil
}

// nullableTextValue renders "" as SQL NULL (the optionalText collapse).
func nullableTextValue(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func nullableTextPtr(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

var customModelDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func nullableDatePtr(value *string) any {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" || !customModelDatePattern.MatchString(trimmed) {
		return nil
	}
	return trimmed
}

func normalizedDateCopy(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" || !customModelDatePattern.MatchString(trimmed) {
		return nil
	}
	return &trimmed
}

// nullableInt64Ptr maps negatives and non-integers to NULL (the
// optionalInteger collapse); the HTTP schema already rejects them.
func nullableInt64Ptr(value *int64) any {
	if value == nil || *value < 0 {
		return nil
	}
	return *value
}

func nullableFloat64Ptr(value *float64) any {
	if value == nil || *value < 0 {
		return nil
	}
	return *value
}

func (s *Store) boolValue(value bool) any {
	return value
}

func (s *Store) nowUTC() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// newCustomModelID mirrors newId('custom_model'):
// custom_model_<millis>_<8 hex>.
func newCustomModelID(now time.Time) string {
	return fmt.Sprintf("custom_model_%d_%s", now.UnixMilli(), randomHex8(now))
}

func randomHex8(now time.Time) string {
	// Node uses Math.random(); any stable entropy source with the same shape
	// is contract-equivalent (the id is opaque).
	nanos := uint64(now.UnixNano())
	alphabet := "0123456789abcdef"
	out := make([]byte, 8)
	for index := range out {
		nanos = nanos*6364136223846793005 + 1442695040888963407
		out[index] = alphabet[(nanos>>60)&0xF]
	}
	return string(out)
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

// capabilityMatrixJSON 序列化显式工具能力矩阵（T82 写侧）：nil 归一为空
// object，满足列 object CHECK（jsonb_typeof = 'object'）；键序由
// json.Marshal 稳定排序，patch 等值比较确定。
func capabilityMatrixJSON(value map[string][]string) string {
	if value == nil {
		value = map[string][]string{}
	}
	return mustJSON(value)
}

// capabilityTokenArrayJSON 序列化显式模态声明（T82 写侧）：nil 归一为空
// array，满足列 array CHECK（jsonb_typeof = 'array'）。
func capabilityTokenArrayJSON(value []string) string {
	if value == nil {
		value = []string{}
	}
	return mustJSON(value)
}

// patchValuesEqual mirrors patchValuesEqual: identity or JSON string
// equality (nil encodes as "null" exactly like JSON.stringify(null)).
func patchValuesEqual(left, right any) bool {
	if left == nil && right == nil {
		return true
	}
	return mustJSON(left) == mustJSON(right)
}

// nextCustomModelUpdatedAt mirrors nextUpdatedAt: never earlier than now and
// strictly after the current stamp.
func nextCustomModelUpdatedAt(current string, now time.Time) string {
	currentMs := parseRfc3339Millis(current)
	nextMs := now.UnixMilli()
	if currentMs != nil && *currentMs+1 > nextMs {
		nextMs = *currentMs + 1
	}
	return time.UnixMilli(nextMs).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// parseRfc3339Millis mirrors rfc3339InstantMilliseconds.
func parseRfc3339Millis(value string) *int64 {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	millis := parsed.UnixMilli()
	return &millis
}

func stringPtr(value string) *string {
	copied := value
	return &copied
}
